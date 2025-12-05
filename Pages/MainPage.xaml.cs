using MyFitnessRPG.Models;

namespace MyFitnessRPG.Views
{
    public partial class MainPage : ContentPage
    {
        public Hero CurrentHero { get; private set; }

        public MainPage()
        {
            InitializeComponent();
        }

        private void OnCreateHeroClicked(object sender, EventArgs e)
        {
            string heroName = HeroNameEntry.Text;
            string heroClass = HeroClassPicker.SelectedItem?.ToString();

            // Basic validation
            if (string.IsNullOrWhiteSpace(heroName))
            {
                ResultLabel.Text = "Please enter a valid hero name.";
                ResultLabel.TextColor = Colors.Red;
                return;
            }

            if (heroClass == null)
            {
                ResultLabel.Text = "Please choose a class.";
                ResultLabel.TextColor = Colors.Red;
                return;
            }

            // Create the hero
            CurrentHero = new Hero
            {
                Name = heroName,
                Class = heroClass
            };

            ResultLabel.TextColor = Colors.Green;
            ResultLabel.Text = $"Hero Created!\n{CurrentHero}";
        }
    }
}
